"""Exception classes for the Omnara SDK."""


class OmnaraError(Exception):
    """Base exception for all Omnara SDK errors."""

    pass


class AuthenticationError(OmnaraError):
    """Raised when authentication fails."""

    pass


class TimeoutError(OmnaraError):
    """Raised when an operation times out."""

    pass


class APIError(OmnaraError):
    """Raised when the API returns an error response."""

    def __init__(self, status_code: int, detail: str):
        self.status_code = status_code
        self.detail = detail
        super().__init__(f"API Error {status_code}: {detail}")
