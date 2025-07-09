#!/bin/bash

# Stop development services for Agent Dashboard

echo "🛑 Stopping development services..."

# Stop Docker container
if sudo docker ps -q -f name=agent-dashboard-db-dev | grep -q .; then
    echo "Stopping PostgreSQL container..."
    sudo docker stop agent-dashboard-db-dev > /dev/null 2>&1
    echo "PostgreSQL stopped"
else
    echo "PostgreSQL container not running"
fi

# Kill any remaining processes on the ports
echo "Cleaning up any remaining processes..."

# Kill processes on port 8000 (backend) 
lsof -ti:8000 | xargs kill -9 2>/dev/null || true

# Kill processes on port 8080 (unified server - MCP + FastAPI)
lsof -ti:8080 | xargs kill -9 2>/dev/null || true

echo "✅ All services stopped"