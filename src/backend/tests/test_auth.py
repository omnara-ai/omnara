"""Tests for authentication endpoints."""

from datetime import datetime, timezone
from uuid import uuid4
from unittest.mock import patch, Mock

from shared.database.models import User, APIKey


class TestAuthEndpoints:
    """Test authentication endpoints."""

    def test_get_session_unauthenticated(self, client):
        """Test getting session when not authenticated."""
        response = client.get("/api/v1/auth/session")
        assert response.status_code == 401
        assert response.json()["detail"] == "Not authenticated"

    def test_get_session_authenticated(self, authenticated_client, test_user):
        """Test getting session when authenticated."""
        response = authenticated_client.get("/api/v1/auth/session")
        assert response.status_code == 200
        data = response.json()
        assert data["id"] == str(test_user.id)
        assert data["email"] == test_user.email
        assert data["display_name"] == test_user.display_name

    def test_get_current_user_profile(self, authenticated_client, test_user):
        """Test getting current user profile."""
        response = authenticated_client.get("/api/v1/auth/me")
        assert response.status_code == 200
        data = response.json()
        assert data["id"] == str(test_user.id)
        assert data["email"] == test_user.email
        assert data["display_name"] == test_user.display_name

    @patch("backend.auth.utils.get_supabase_service_client")
    def test_update_user_profile(
        self, mock_supabase, authenticated_client, test_user, test_db
    ):
        """Test updating user profile."""
        # Mock Supabase client
        mock_client = Mock()
        mock_client.auth.admin.update_user_by_id = Mock()
        mock_supabase.return_value = mock_client

        new_display_name = "Updated Test User"
        response = authenticated_client.patch(
            "/api/v1/auth/me", json={"display_name": new_display_name}
        )
        assert response.status_code == 200
        data = response.json()
        assert data["display_name"] == new_display_name

        # Verify in database
        test_db.refresh(test_user)
        assert test_user.display_name == new_display_name

        # Verify Supabase was called
        mock_client.auth.admin.update_user_by_id.assert_called_once()

    def test_sync_user(self, authenticated_client, test_user, test_db):
        """Test syncing user from Supabase."""
        response = authenticated_client.post(
            "/api/v1/auth/sync-user",
            json={
                "id": str(test_user.id),
                "email": test_user.email,
                "display_name": "Synced Name",
            },
        )
        assert response.status_code == 200
        assert response.json()["message"] == "User synced successfully"

        # Verify display name was updated
        test_db.refresh(test_user)
        assert test_user.display_name == "Synced Name"

    def test_sync_user_forbidden(self, authenticated_client, test_user):
        """Test syncing a different user is forbidden."""
        different_user_id = str(uuid4())
        response = authenticated_client.post(
            "/api/v1/auth/sync-user",
            json={
                "id": different_user_id,
                "email": "other@example.com",
                "display_name": "Other User",
            },
        )
        assert response.status_code == 403
        assert response.json()["detail"] == "Cannot sync different user"


class TestAPIKeyEndpoints:
    """Test API key management endpoints."""

    @patch("backend.auth.routes.create_api_key_jwt")
    def test_create_api_key(
        self, mock_create_jwt, authenticated_client, test_user, test_db
    ):
        """Test creating an API key."""
        mock_jwt_token = "test.jwt.token"
        mock_create_jwt.return_value = mock_jwt_token

        response = authenticated_client.post(
            "/api/v1/auth/api-keys",
            json={"name": "Test API Key", "expires_in_days": 30},
        )

        assert response.status_code == 200
        data = response.json()
        assert data["name"] == "Test API Key"
        assert data["api_key"] == mock_jwt_token
        assert "expires_at" in data

        # Verify in database
        api_key = test_db.query(APIKey).filter(APIKey.user_id == test_user.id).first()
        assert api_key is not None
        assert api_key.name == "Test API Key"
        assert api_key.is_active is True

    @patch("backend.auth.routes.create_api_key_jwt")
    def test_create_api_key_max_limit(
        self, mock_create_jwt, authenticated_client, test_user, test_db
    ):
        """Test creating API key when at max limit."""
        mock_create_jwt.return_value = "test.jwt.token"

        # Create 50 existing API keys
        for i in range(50):
            api_key = APIKey(
                id=uuid4(),
                user_id=test_user.id,
                name=f"Key {i}",
                api_key_hash="hash",
                api_key=f"token{i}",
                is_active=True,
                created_at=datetime.now(timezone.utc),
            )
            test_db.add(api_key)
        test_db.commit()

        response = authenticated_client.post(
            "/api/v1/auth/api-keys",
            json={"name": "One Too Many", "expires_in_days": 30},
        )

        assert response.status_code == 400
        assert "Maximum of 50 active API keys allowed" in response.json()["detail"]

    def test_list_api_keys(self, authenticated_client, test_user, test_db):
        """Test listing API keys."""
        # Create test API keys
        api_keys = []
        for i in range(3):
            api_key = APIKey(
                id=uuid4(),
                user_id=test_user.id,
                name=f"Key {i}",
                api_key_hash=f"hash{i}",
                api_key=f"token{i}",
                is_active=i != 2,  # Last one is inactive
                created_at=datetime.now(timezone.utc),
            )
            api_keys.append(api_key)
            test_db.add(api_key)
        test_db.commit()

        response = authenticated_client.get("/api/v1/auth/api-keys")
        assert response.status_code == 200
        data = response.json()
        assert len(data) == 3

        # Should be ordered by created_at desc
        assert data[0]["name"] == "Key 2"
        assert data[0]["is_active"] is False
        assert data[1]["name"] == "Key 1"
        assert data[1]["is_active"] is True

    def test_revoke_api_key(self, authenticated_client, test_user, test_db):
        """Test revoking an API key."""
        # Create test API key
        api_key = APIKey(
            id=uuid4(),
            user_id=test_user.id,
            name="Test Key",
            api_key_hash="hash",
            api_key="token",
            is_active=True,
            created_at=datetime.now(timezone.utc),
        )
        test_db.add(api_key)
        test_db.commit()

        response = authenticated_client.delete(f"/api/v1/auth/api-keys/{api_key.id}")
        assert response.status_code == 200
        assert response.json()["message"] == "API key revoked successfully"

        # Verify in database
        test_db.refresh(api_key)
        assert api_key.is_active is False

    def test_revoke_api_key_not_found(self, authenticated_client):
        """Test revoking a non-existent API key."""
        fake_id = str(uuid4())
        response = authenticated_client.delete(f"/api/v1/auth/api-keys/{fake_id}")
        assert response.status_code == 404
        assert response.json()["detail"] == "API key not found"

    def test_revoke_api_key_wrong_user(self, authenticated_client, test_db):
        """Test revoking another user's API key."""
        # Create another user
        other_user = User(
            id=uuid4(),
            email="other@example.com",
            display_name="Other User",
            created_at=datetime.now(timezone.utc),
            updated_at=datetime.now(timezone.utc),
        )
        test_db.add(other_user)

        # Create API key for other user
        api_key = APIKey(
            id=uuid4(),
            user_id=other_user.id,
            name="Other's Key",
            api_key_hash="hash",
            api_key="token",
            is_active=True,
            created_at=datetime.now(timezone.utc),
        )
        test_db.add(api_key)
        test_db.commit()

        response = authenticated_client.delete(f"/api/v1/auth/api-keys/{api_key.id}")
        assert response.status_code == 404
        assert response.json()["detail"] == "API key not found"
