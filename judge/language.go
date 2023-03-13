package judge

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/pkg/errors"
	"github.com/samber/lo"

	"proctor-signal/config"
	"proctor-signal/model"
)

// Regex
var (
	variableRegex = regexp.MustCompile(`\$\{([\w_]+)}|\$([\w_]+)`)
	validVar      = regexp.MustCompile(`^'([^']*)'|"([^"]*)"|([^'"]*)$`)
)

type languageConf struct {
	raw config.LanguageConfEntity

	env             map[string]string
	vars            map[string]string
	needCompilation bool
}

func extractVar(s string) string {
	part := variableRegex.FindStringSubmatch(s)
	return lo.Compact(part[1:])[0]
}

func split(s string) []string {
	return lo.Filter(
		strings.Split(s, " "),
		func(s string, _ int) bool { return s != "" },
	)
}

func pair(k, v string) lo.Entry[string, string] {
	return lo.Entry[string, string]{Key: k, Value: v}
}

func parseEnv(env string) (key string, val string, err error) {
	key, val, ok := strings.Cut(env, "=")
	if !ok {
		return "", "", errors.Errorf("invalid environment string: %s", env)
	}
	// Trim quotes.
	if !validVar.MatchString(val) {
		return "", "", errors.Errorf("invalid environment string: %s", env)
	}
	val = lo.Compact(validVar.FindStringSubmatch(val)[1:])[0]
	return
}

func newLanguageConfigs(conf config.LanguageConf, judgeEnvs []string) (map[string]*languageConf, error) {
	env := make(map[string]string, len(judgeEnvs))
	for _, judgeEnv := range judgeEnvs {
		k, v, err := parseEnv(judgeEnv)
		if err != nil {
			return nil, errors.Errorf("invalid environ item: %s", judgeEnv)
		}
		env[k] = v
	}

	res := make(map[string]*languageConf, len(conf))
	for language, raw := range conf {
		l := &languageConf{
			// Copy the env map to avoid unintentional access.
			vars: lo.Assign(env),
			env:  lo.Assign(env),
			raw:  raw,
		}
		res[language] = l
		if err := l.normalize(); err != nil {
			return nil, errors.Errorf("an error occurred when loading config for language %s", language)
		}
	}

	return res, nil
}

func (l *languageConf) eval(val string, extra ...lo.Entry[string, string]) string {
	table := lo.Assign(l.vars, lo.FromEntries(extra))
	return variableRegex.ReplaceAllStringFunc(val, func(s string) string {
		// ${Var} or $Var.
		toReplace := extractVar(s)
		if replaced, has := table[toReplace]; has {
			return replaced
		}
		return s
	})
}

func (l *languageConf) normalize() error {
	// Normalize env.
	for _, envStr := range l.raw.Environment {
		key, val, err := parseEnv(envStr)
		if err != nil {
			return err
		}
		val = l.eval(val)

		l.env[key] = val
		l.vars[key] = val
	}

	l.needCompilation = !l.raw.NoCompilation
	l.vars["Compiler"] = l.eval(l.raw.Compiler)
	l.vars["SourceName"] = l.eval(l.raw.SourceName)
	l.vars["ArtifactName"] = l.eval(l.raw.ArtifactName)
	l.vars["CompileCmd"] = l.eval(l.raw.CompileCmd)
	l.vars["ExecuteCmd"] = l.eval(l.raw.ExecuteCmd)
	return nil
}

func (l *languageConf) evalCompileCmd(sub *model.Submission) ([]string, error) {
	var compileOptKeys []string
	if strings.TrimSpace(sub.CompilerOption) != "" {
		if err := json.Unmarshal([]byte(sub.CompilerOption), &compileOptKeys); err != nil {
			return nil, errors.WithMessagef(err, "failed to unmarshal compile options")
		}
	}

	for i, key := range compileOptKeys {
		opt, ok := l.raw.Options[key]
		if !ok {
			return nil, fmt.Errorf("compile option `%s` not found, possibly a configuration mistake", key)
		}
		compileOptKeys[i] = opt
	}

	return split(l.eval(
		l.vars["CompileCmd"], pair("Args", strings.Join(compileOptKeys, " ")),
	)), nil
}

func (l *languageConf) getRunCmd() []string {
	return split(l.vars["ExecuteCmd"])
}

func (l *languageConf) getEnvs() []string {
	return lo.Map(lo.Entries(l.env), func(ent lo.Entry[string, string], _ int) string {
		return fmt.Sprintf(`%s=%s`, ent.Key, ent.Value)
	})
}

func (l *languageConf) getSourceName() string {
	return l.vars["SourceName"]
}

func (l *languageConf) getArtifactName() string {
	return l.vars["ArtifactName"]
}

func (l *languageConf) needCompile() bool {
	return l.needCompilation
}
