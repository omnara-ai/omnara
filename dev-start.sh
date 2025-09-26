#!/bin/bash

# Development startup script for Agent Dashboard
# Runs PostgreSQL in Docker, everything else locally for live development

set -e

# Parse command line arguments
RESET_DB=false
while [[ $# -gt 0 ]]; do
    case $1 in
        --reset-db)
            RESET_DB=true
            shift
            ;;
        *)
            echo "Unknown option: $1"
            echo "Usage: $0 [--reset-db]"
            exit 1
            ;;
    esac
done

echo "🚀 Starting Agent Dashboard in development mode..."

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Function to cleanup on exit
cleanup() {
    echo -e "\n${YELLOW}Cleaning up...${NC}"

    # Kill background processes
    if [[ -n $VIEWER_PID ]]; then
        echo "Stopping relay viewer..."
        kill $VIEWER_PID 2>/dev/null || true
        wait $VIEWER_PID 2>/dev/null || true
    fi
    if [[ -n $RELAY_PID ]]; then
        echo "Stopping SSH relay..."
        kill $RELAY_PID 2>/dev/null || true
        wait $RELAY_PID 2>/dev/null || true
    fi
    if [[ -n $APP_PID ]]; then
        echo "Stopping unified server..."
        kill $APP_PID 2>/dev/null || true
    fi
    
    if [[ -n $BACKEND_PID ]]; then
        echo "Stopping backend..."
        kill $BACKEND_PID 2>/dev/null || true
    fi
    
    
    echo -e "${GREEN}Cleanup complete${NC}"
    exit 0
}

# Set up signal handlers
trap cleanup SIGINT SIGTERM

# Check if Docker is running
if ! sudo docker info > /dev/null 2>&1; then
    echo -e "${RED}Error: Docker is not running. Please start Docker first.${NC}"
    exit 1
fi

# Set up data directory for PostgreSQL persistence
DATA_DIR="$(pwd)/postgres-data"
if [ "$RESET_DB" = true ]; then
    echo -e "${YELLOW}Resetting database...${NC}"
    if [ -d "$DATA_DIR" ]; then
        echo -e "${YELLOW}Removing existing PostgreSQL data directory...${NC}"
        sudo rm -rf "$DATA_DIR"
    fi
fi

# Create data directory if it doesn't exist
if [ ! -d "$DATA_DIR" ]; then
    echo -e "${BLUE}Creating PostgreSQL data directory...${NC}"
    mkdir -p "$DATA_DIR"
fi

# Start PostgreSQL in Docker
echo -e "${BLUE}Starting PostgreSQL in Docker...${NC}"
sudo docker run -d \
    --name agent-dashboard-db-dev \
    --rm \
    -p 5432:5432 \
    -e POSTGRES_USER=user \
    -e POSTGRES_PASSWORD=password \
    -e POSTGRES_DB=agent_dashboard \
    -v "$DATA_DIR:/var/lib/postgresql/data" \
    postgres:16-alpine > /dev/null

# Wait for PostgreSQL to be ready
echo -e "${YELLOW}Waiting for PostgreSQL to be ready...${NC}"
for i in {1..30}; do
    if sudo docker exec agent-dashboard-db-dev pg_isready -U user -d agent_dashboard > /dev/null 2>&1; then
        echo -e "${GREEN}PostgreSQL is ready!${NC}"
        break
    fi
    sleep 1
    if [ $i -eq 30 ]; then
        echo -e "${RED}Error: PostgreSQL failed to start${NC}"
        sudo docker stop agent-dashboard-db-dev > /dev/null 2>&1 || true
        exit 1
    fi
done

# Initialize database
echo -e "${BLUE}Initializing database...${NC}"
export ENVIRONMENT="development"
export DEVELOPMENT_DB_URL="postgresql://user:password@localhost:5432/agent_dashboard"
./infrastructure/scripts/init-db.sh

# Prepare log directory
LOG_DIR="$(pwd)/logs"
mkdir -p "$LOG_DIR"
RELAY_LOG="$LOG_DIR/relay.log"
> "$RELAY_LOG"
VIEWER_LOG="$LOG_DIR/relay-viewer.log"
> "$VIEWER_LOG"

# Start SSH relay server
echo -e "${BLUE}Starting SSH relay server...${NC}"
export PYTHONPATH="$(pwd)/src"
export OMNARA_RELAY_SSH_HOST="0.0.0.0"
export OMNARA_RELAY_HOST="127.0.0.1"
export OMNARA_RELAY_WS_PORT=8787
python -m relay_ssh_server.run >> "$RELAY_LOG" 2>&1 &
RELAY_PID=$!
sleep 2

# Start unified server (MCP + FastAPI)
echo -e "${BLUE}Starting unified server (MCP + FastAPI)...${NC}"
export PYTHONPATH="$(pwd)/src"
export API_PORT=8080
export MCP_SERVER_PORT=8080
python -m servers.app &
APP_PID=$!

# Wait a moment for unified server to start
sleep 2

# Start Backend API
echo -e "${BLUE}Starting Backend API...${NC}"
export PYTHONPATH="$(pwd)/src"
export API_PORT=8000
uvicorn backend.main:app --host 0.0.0.0 --port 8000 &
BACKEND_PID=$!

# Wait a moment for backend to start
sleep 2

# Start relay viewer (static test client)
echo -e "${BLUE}Starting relay viewer...${NC}"
if [ ! -d "apps/relay-viewer/node_modules" ]; then
    (cd apps/relay-viewer && npm install >/dev/null 2>&1)
fi
npm --prefix apps/relay-viewer start >> "$VIEWER_LOG" 2>&1 &
VIEWER_PID=$!
sleep 2

echo -e "${GREEN}🎉 All services started successfully!${NC}"
echo -e "${BLUE}Services:${NC}"
echo -e "  🔌 SSH Relay:      ssh://localhost:2222  ws://localhost:${OMNARA_RELAY_WS_PORT}"
echo -e "  🔧 Backend API:     http://localhost:8000"
echo -e "  🤖 Unified Server:  http://localhost:8080 (MCP + FastAPI)"
echo -e "  🖥️  Relay Viewer:    http://localhost:4173"
echo -e "  🗄️  PostgreSQL:      localhost:5432"
echo -e "  📜 Relay Log:       $RELAY_LOG"
echo -e "  📜 Viewer Log:      $VIEWER_LOG"
echo -e "\n${YELLOW}Press Ctrl+C to stop all services${NC}"

# Wait for all background processes
wait
