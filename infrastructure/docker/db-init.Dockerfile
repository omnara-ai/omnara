FROM python:3.12-slim

WORKDIR /app

# Copy shared requirements
COPY src/shared/requirements.txt /app/shared/requirements.txt
RUN pip install --no-cache-dir -r shared/requirements.txt

# Copy shared code
COPY src/shared /app/shared

# Set Python path
ENV PYTHONPATH=/app