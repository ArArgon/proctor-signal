#include <stdio.h>

int main() {
    char s[16];

    FILE *in = fopen("input", "r");
    fscanf(in, "%s", s);

    FILE *out = fopen("output", "w");
    fprintf(out, "%s", s);

    return 0;
}
