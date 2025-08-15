#!/usr/bin/env python3
"""
Test runner for AmpWrapper tests
Runs all test suites and provides summary
"""

import sys
import unittest
import time
from pathlib import Path

# Add project root to path
project_root = Path(__file__).parent.parent
sys.path.insert(0, str(project_root))


def run_test_suite(test_module_name, description):
    """Run a specific test suite"""
    print(f"\n{'=' * 60}")
    print(f"Running {description}")
    print("=" * 60)

    try:
        # Import and run the test module
        loader = unittest.TestLoader()
        suite = loader.loadTestsFromName(test_module_name)

        runner = unittest.TextTestRunner(verbosity=2)
        result = runner.run(suite)

        return result.wasSuccessful(), len(result.errors), len(result.failures)

    except Exception as e:
        print(f"ERROR: Failed to run {test_module_name}: {e}")
        return False, 1, 0


def main():
    """Run all test suites"""
    print("AmpWrapper Test Suite Runner")
    print(f"Timestamp: {time.strftime('%Y-%m-%d %H:%M:%S')}")

    test_suites = [
        ("test_amp_wrapper_unit", "Unit Tests"),
        ("test_amp_wrapper_mocks", "Mock Tests"),
        ("test_amp_wrapper_integration", "Integration Tests"),
        ("test_amp_wrapper_regression", "Regression Tests"),
    ]

    total_success = True
    total_errors = 0
    total_failures = 0

    start_time = time.time()

    for module_name, description in test_suites:
        success, errors, failures = run_test_suite(module_name, description)
        total_success = total_success and success
        total_errors += errors
        total_failures += failures

    elapsed = time.time() - start_time

    print(f"\n{'=' * 60}")
    print("TEST SUMMARY")
    print("=" * 60)
    print(f"Total runtime: {elapsed:.2f} seconds")
    print(f"Overall result: {'PASS' if total_success else 'FAIL'}")
    print(f"Total errors: {total_errors}")
    print(f"Total failures: {total_failures}")

    if not total_success:
        print("\n❌ Some tests failed. Check output above for details.")
        sys.exit(1)
    else:
        print("\n✅ All tests passed!")
        sys.exit(0)


if __name__ == "__main__":
    main()
