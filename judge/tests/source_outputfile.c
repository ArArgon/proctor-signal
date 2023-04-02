#include <stdio.h>

int main() {
    char s[16];
    scanf("%s", s);

    FILE *f = fopen("output", "w");
    fprintf(f, "%s", s);

    return 0;
}
