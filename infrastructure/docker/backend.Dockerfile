FROM python:3.12-slim

WORKDIR /app

# Copy requirements
COPY src/backend/requirements.txt /app/backend/requirements.txt
COPY src/shared/requirements.txt /app/shared/requirements.txt

# Install dependencies
RUN pip install --no-cache-dir -r backend/requirements.txt

# Copy application code
COPY src/shared /app/shared
COPY src/backend /app/backend
COPY src/servers /app/servers

# Set Python path
ENV PYTHONPATH=/app

# Run the backend from root directory to access shared
CMD ["python", "-m", "backend.main"]