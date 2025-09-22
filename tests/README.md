# tests/

This directory contains all test suites for the Omnara project in a flat structure.

## Structure

Since we currently have a small number of tests, they're organized in a flat structure for simplicity:

- **`test_*.py`** - All test files
  - Mix of unit and integration tests
  - Clear naming indicates test type and purpose

- **`fixtures/`** - Test fixtures and sample data
  - Shared test data and mock objects
  - Reusable test configurations

- **`run_tests.py`** - Main test runner script
  - Provides unified interface for running all tests
  - Handles test environment setup

## Running Tests

```bash
# All tests
make test

# Specific test file
pytest tests/test_amp_wrapper_unit.py

# Pattern-based filtering
pytest tests/ -k "unit"
pytest tests/ -k "integration"

# With coverage
pytest --cov=src tests/
```

As the test suite grows, we can reorganize into `unit/` and `integration/` subdirectories.