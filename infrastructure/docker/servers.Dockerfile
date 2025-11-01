FROM python:3.12-slim

WORKDIR /app

# Copy requirements
COPY src/servers/requirements.txt /app/servers/requirements.txt
COPY src/shared/requirements.txt /app/shared/requirements.txt

# Install dependencies
RUN pip install --no-cache-dir -r servers/requirements.txt

# Copy application code
COPY src/shared /app/shared
COPY src/servers /app/servers

# Set Python path
ENV PYTHONPATH=/app

# Run the unified server (combines MCP + REST API)
CMD ["python", "-m", "servers.app"]