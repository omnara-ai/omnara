FROM python:3.12-slim

WORKDIR /app

# Copy requirements
COPY backend/requirements.txt /app/backend/requirements.txt
COPY shared/requirements.txt /app/shared/requirements.txt

# Install dependencies
RUN pip install --no-cache-dir -r backend/requirements.txt

# Copy application code
COPY shared /app/shared
COPY backend /app/backend

# Set Python path
ENV PYTHONPATH=/app

# Run the backend from root directory to access shared
CMD ["python", "-m", "backend.main"]