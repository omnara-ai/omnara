FROM python:3.12-slim

WORKDIR /app

# Copy requirements
COPY src/shared/requirements.txt /app/shared/requirements.txt

# Install dependencies (relay uses shared requirements + fastapi/uvicorn/aiohttp)
RUN pip install --no-cache-dir -r shared/requirements.txt && \
    pip install --no-cache-dir \
    fastapi==0.115.12 \
    uvicorn[standard]==0.34.3 \
    aiohttp>=3.9.0 \
    python-jose[cryptography]==3.5.0 \
    supabase==2.15.3

# Copy application code
COPY src/shared /app/shared
COPY src/relay_server /app/relay_server

# Set Python path
ENV PYTHONPATH=/app

# Expose WebSocket port
EXPOSE 8787

# Run the relay server
CMD ["python", "-m", "relay_server.app"]
