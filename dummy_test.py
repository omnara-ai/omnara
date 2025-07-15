#!/usr/bin/env python3

def hello_world():
    return "Hello, World!"

def add_numbers(a, b):
    return a + b

def test_function():
    print("Running tests...")
    assert hello_world() == "Hello, World!"
    assert add_numbers(2, 3) == 5
    assert add_numbers(-1, 1) == 0
    print("All tests passed!")

if __name__ == "__main__":
    print(hello_world())
    print(f"2 + 3 = {add_numbers(2, 3)}")
    test_function()