FROM python:3.12-slim

WORKDIR /app

# Copy requirements
COPY servers/requirements.txt /app/servers/requirements.txt
COPY shared/requirements.txt /app/shared/requirements.txt

# Install dependencies
RUN pip install --no-cache-dir -r servers/requirements.txt

# Copy application code
COPY shared /app/shared
COPY servers /app/servers

# Set Python path
ENV PYTHONPATH=/app

# Run the MCP server from root directory to access shared
CMD ["python", "-m", "servers.mcp_server.server"]