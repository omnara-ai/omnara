from collections.abc import Generator

from sqlalchemy import create_engine
from sqlalchemy.orm import Session, sessionmaker

from ..config.settings import settings

# For Supabase session mode pooler (port 5432)
# Keep pool small since Supabase pooler has 15 connection limit
engine = create_engine(
    settings.database_url,
    pool_size=3,  # Small pool size to stay under Supabase's 15 limit
    max_overflow=2,  # Allow 2 overflow connections (total: 5)
    pool_timeout=30,  # Wait up to 30 seconds for connection
    pool_recycle=1800,  # Recycle connections after 30 minutes
    pool_pre_ping=True,  # Test connections before using them
)
SessionLocal = sessionmaker(autocommit=False, autoflush=False, bind=engine)


def get_db() -> Generator[Session, None, None]:
    """Get database session"""
    db = SessionLocal()
    try:
        yield db
    finally:
        db.close()
