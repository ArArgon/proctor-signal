#include <iostream>

template <typename T>
auto add(T a, T b) {
    auto f = [] (T x, T y) {
        return x + y;
    };
    return f(a, b);
}

int main() {
    std::cout << add(997ll, 8888888888ll) << std::endl;
    return 0;
}