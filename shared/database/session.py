from collections.abc import Generator

from sqlalchemy import create_engine
from sqlalchemy.orm import Session, sessionmaker

from ..config.settings import settings

# For Supabase session mode pooler (port 5432)
# Single process configuration - maximize pool usage
engine = create_engine(
    settings.database_url,
    pool_size=10,  # Use most of the 15 connection limit
    max_overflow=4,  # Allow overflow up to 14 total (under 15 limit)
    pool_timeout=10,  # Fast timeout for better responsiveness
    pool_recycle=3600,  # Recycle after 1 hour
    pool_pre_ping=False,  # Skip pre-ping since Supabase pooler handles connection health
)
SessionLocal = sessionmaker(autocommit=False, autoflush=False, bind=engine)


def get_db() -> Generator[Session, None, None]:
    """Get database session"""
    db = SessionLocal()
    try:
        yield db
    finally:
        db.close()
