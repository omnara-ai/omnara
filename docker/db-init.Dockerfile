FROM python:3.12-slim

WORKDIR /app

# Copy shared requirements
COPY shared/requirements.txt /app/shared/requirements.txt
RUN pip install --no-cache-dir -r shared/requirements.txt

# Copy shared code
COPY shared /app/shared

# Set Python path
ENV PYTHONPATH=/app