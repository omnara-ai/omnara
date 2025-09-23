"""Tests for team management endpoints."""

from datetime import datetime, timezone
from uuid import uuid4

from shared.database.models import User
from shared.database.enums import TeamRole


def _create_team(authenticated_client):
    response = authenticated_client.post("/api/v1/teams", json={"name": "Collab Crew"})
    assert response.status_code == 201
    return response.json()


def test_create_team_and_list(authenticated_client):
    """Creating a team should return detail and list it for the owner."""
    team = _create_team(authenticated_client)

    # Owner should be returned in members list
    assert team["name"] == "Collab Crew"
    assert team["role"] == TeamRole.OWNER.value
    assert len(team["members"]) == 1
    owner_membership = team["members"][0]
    assert owner_membership["role"] == TeamRole.OWNER.value
    assert owner_membership["invited"] is False

    # Listing teams returns summary with membership count
    list_response = authenticated_client.get("/api/v1/teams")
    assert list_response.status_code == 200
    summaries = list_response.json()
    assert len(summaries) == 1
    summary = summaries[0]
    assert summary["id"] == team["id"]
    assert summary["member_count"] == 1
    assert summary["role"] == TeamRole.OWNER.value

    # Fetching members returns same data
    detail_response = authenticated_client.get(f"/api/v1/teams/{team['id']}/members")
    assert detail_response.status_code == 200
    detail = detail_response.json()
    assert detail["members"] == team["members"]


def test_add_member_existing_user(authenticated_client, test_db):
    """Adding a member with an existing account should link by user_id."""
    team = _create_team(authenticated_client)

    # Create another user directly in the DB
    other_user = User(
        id=uuid4(),
        email="teammate@example.com",
        display_name="Teammate",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
    )
    test_db.add(other_user)
    test_db.commit()

    response = authenticated_client.post(
        f"/api/v1/teams/{team['id']}/members",
        json={"email": other_user.email, "role": TeamRole.ADMIN.value},
    )
    assert response.status_code == 201
    membership = response.json()
    assert membership["user_id"] == str(other_user.id)
    assert membership["email"] == other_user.email
    assert membership["role"] == TeamRole.ADMIN.value
    assert membership["invited"] is False

    # Listing members should include the new admin
    detail_response = authenticated_client.get(f"/api/v1/teams/{team['id']}/members")
    members = detail_response.json()["members"]
    assert len(members) == 2
    admin_entry = next(m for m in members if m["user_id"] == str(other_user.id))
    assert admin_entry["role"] == TeamRole.ADMIN.value


def test_add_member_placeholder_for_unknown_email(authenticated_client):
    """Unknown email addresses should be stored as pending placeholders."""
    team = _create_team(authenticated_client)

    response = authenticated_client.post(
        f"/api/v1/teams/{team['id']}/members",
        json={"email": "future-user@example.com"},
    )
    assert response.status_code == 201
    membership = response.json()
    assert membership["user_id"] is None
    assert membership["email"] == "future-user@example.com"
    assert membership["invited"] is True
    assert membership["role"] == TeamRole.MEMBER.value

    detail_response = authenticated_client.get(f"/api/v1/teams/{team['id']}/members")
    members = detail_response.json()["members"]
    assert len(members) == 2
    placeholder = next(m for m in members if m["user_id"] is None)
    assert placeholder["email"] == "future-user@example.com"
    assert placeholder["invited"] is True
