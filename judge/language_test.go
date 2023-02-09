package judge

import (
	"reflect"
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"golang.org/x/exp/constraints"
	"golang.org/x/exp/slices"

	"proctor-signal/config"
	"proctor-signal/model"
)

func unorderedCompare[Elem constraints.Ordered](arr1 []Elem, arr2 []Elem) bool {
	if len(arr1) != len(arr2) {
		return false
	}
	slices.Sort(arr1)
	slices.Sort(arr2)
	return reflect.DeepEqual(arr1, arr2)
}

func TestLanguageConfig(t *testing.T) {
	rawConfs := config.LanguageConf{
		"cpp": config.LanguageConfEntity{
			SourceName:   "source.cpp",
			ArtifactName: "main",
			Compiler:     "/usr/bin/g++",
			CompileCmd:   "$Compiler ${Args} $SourceName -o ${ArtifactName}",
			ExecuteCmd:   "main",
			Options: map[string]string{
				"o2":    "-O2",
				"cpp14": "--std=c++14",
			},
		},
		"java": config.LanguageConfEntity{
			SourceName:   "Solution.java",
			ArtifactName: "Solution.class",
			Compiler:     "${JAVA_HOME}/bin/javac",
			CompileCmd:   "$Compiler $Args $SourceName",
			ExecuteCmd:   "$JAVA_HOME/bin/java -jar $ArtifactName",
			Environment: []string{`JAVA_HOME="/usr/lib/jvm/java-17-openjdk-amd64"`,
				`ENV_TEST='$JAVA_HOME/../'`,
				`X=Y$MyEnv`,
				"PATH=$PATH:${JAVA_HOME}/bin",
			},
			Options: map[string]string{
				"java11": "--source 11",
				"java8":  "--source 8",
			},
		},
	}

	confs, err := newLanguageConfigs(rawConfs, []string{"PATH=/usr/bin:/bin", "MyEnv='a'"})
	assert.NoError(t, err)

	t.Run("cpp", func(t *testing.T) {
		conf := confs["cpp"]
		assert.Equal(t, []string{"main"}, conf.getRunCmd())
		assert.Equal(t,
			[]string{"/usr/bin/g++", "source.cpp", "-o", "main"},
			lo.Must(conf.evalCompileCmd(&model.Submission{CompilerOption: ""})),
		)
		assert.Equal(t,
			[]string{"/usr/bin/g++", "source.cpp", "-o", "main"},
			lo.Must(conf.evalCompileCmd(&model.Submission{CompilerOption: "[]"})),
		)
		assert.Equal(t,
			[]string{"/usr/bin/g++", "-O2", "--std=c++14", "source.cpp", "-o", "main"},
			lo.Must(conf.evalCompileCmd(&model.Submission{CompilerOption: `["o2", "cpp14"]`})),
		)
		assert.True(t, unorderedCompare(
			[]string{"PATH=/usr/bin:/bin", "MyEnv=a"},
			conf.getEnvs(),
		))
		_, err := conf.evalCompileCmd(&model.Submission{CompilerOption: `["o2", "cpp11"]`})
		assert.ErrorContains(t, err, "compile option `cpp11` not found")
	})

	t.Run("java", func(t *testing.T) {
		conf := confs["java"]
		assert.Equal(t,
			[]string{"/usr/lib/jvm/java-17-openjdk-amd64/bin/java", "-jar", "Solution.class"},
			conf.getRunCmd(),
		)
		assert.Equal(t,
			[]string{"/usr/lib/jvm/java-17-openjdk-amd64/bin/javac", "Solution.java"},
			lo.Must(conf.evalCompileCmd(&model.Submission{CompilerOption: ""})),
		)
		assert.Equal(t,
			[]string{"/usr/lib/jvm/java-17-openjdk-amd64/bin/javac", "--source", "11", "Solution.java"},
			lo.Must(conf.evalCompileCmd(&model.Submission{CompilerOption: `["java11"]`})),
		)

		assert.True(t, unorderedCompare(
			[]string{`JAVA_HOME=/usr/lib/jvm/java-17-openjdk-amd64`,
				`ENV_TEST=/usr/lib/jvm/java-17-openjdk-amd64/../`, "MyEnv=a",
				`X=Ya`, "PATH=/usr/bin:/bin:/usr/lib/jvm/java-17-openjdk-amd64/bin",
			},
			conf.getEnvs(),
		))
	})
}
