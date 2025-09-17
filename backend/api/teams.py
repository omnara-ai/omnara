from uuid import UUID

from fastapi import APIRouter, Depends, HTTPException, status
from fastapi.responses import Response
from sqlalchemy.orm import Session

from shared.database.models import User

from ..auth.dependencies import get_current_user
from ..db.queries import (
    add_team_member,
    create_team,
    delete_team,
    get_team_detail,
    get_user_teams,
    remove_team_member,
    update_team,
    update_team_member_role,
)
from ..models import (
    TeamCreateRequest,
    TeamDetailResponse,
    TeamMemberAddRequest,
    TeamMemberResponse,
    TeamMemberRoleUpdateRequest,
    TeamSummary,
    TeamUpdateRequest,
)
from shared.database.session import get_db

router = APIRouter(prefix="/teams", tags=["teams"])


@router.get("", response_model=list[TeamSummary])
def list_teams(
    current_user: User = Depends(get_current_user),
    db: Session = Depends(get_db),
):
    teams = get_user_teams(db, current_user.id)
    return teams


@router.post("", response_model=TeamDetailResponse, status_code=status.HTTP_201_CREATED)
def create_team_endpoint(
    request: TeamCreateRequest,
    current_user: User = Depends(get_current_user),
    db: Session = Depends(get_db),
):
    try:
        team = create_team(db, current_user, request.name)
        db.commit()
        return team
    except Exception as exc:
        db.rollback()
        raise HTTPException(status_code=400, detail=str(exc)) from exc


@router.get("/{team_id}/members", response_model=TeamDetailResponse)
def get_team_members_endpoint(
    team_id: UUID,
    current_user: User = Depends(get_current_user),
    db: Session = Depends(get_db),
):
    team = get_team_detail(db, team_id, current_user.id)
    if not team:
        raise HTTPException(status_code=404, detail="Team not found")
    return team


@router.patch("/{team_id}", response_model=TeamSummary)
def update_team_endpoint(
    team_id: UUID,
    request: TeamUpdateRequest,
    current_user: User = Depends(get_current_user),
    db: Session = Depends(get_db),
):
    try:
        result = update_team(db, team_id, current_user.id, request.name)
        if result is None:
            db.rollback()
            raise HTTPException(status_code=404, detail="Team not found")
        db.commit()
        return result
    except PermissionError as exc:
        db.rollback()
        raise HTTPException(status_code=403, detail=str(exc)) from exc


@router.delete("/{team_id}", status_code=status.HTTP_204_NO_CONTENT)
def delete_team_endpoint(
    team_id: UUID,
    current_user: User = Depends(get_current_user),
    db: Session = Depends(get_db),
):
    try:
        deleted = delete_team(db, team_id, current_user.id)
        if not deleted:
            db.rollback()
            raise HTTPException(status_code=404, detail="Team not found")
        db.commit()
        return Response(status_code=status.HTTP_204_NO_CONTENT)
    except PermissionError as exc:
        db.rollback()
        raise HTTPException(status_code=403, detail=str(exc)) from exc


@router.post(
    "/{team_id}/members",
    response_model=TeamMemberResponse,
    status_code=status.HTTP_201_CREATED,
)
def add_team_member_endpoint(
    team_id: UUID,
    request: TeamMemberAddRequest,
    current_user: User = Depends(get_current_user),
    db: Session = Depends(get_db),
):
    try:
        member = add_team_member(
            db,
            team_id=team_id,
            acting_user_id=current_user.id,
            email=request.email,
            role=request.role,
        )
        db.commit()
        return member
    except PermissionError as exc:
        db.rollback()
        raise HTTPException(status_code=403, detail=str(exc)) from exc
    except ValueError as exc:
        db.rollback()
        raise HTTPException(status_code=400, detail=str(exc)) from exc


@router.patch(
    "/{team_id}/members/{membership_id}",
    response_model=TeamMemberResponse,
)
def update_team_member_role_endpoint(
    team_id: UUID,
    membership_id: UUID,
    request: TeamMemberRoleUpdateRequest,
    current_user: User = Depends(get_current_user),
    db: Session = Depends(get_db),
):
    try:
        updated = update_team_member_role(
            db,
            team_id=team_id,
            membership_id=membership_id,
            acting_user_id=current_user.id,
            new_role=request.role,
        )
        if updated is None:
            db.rollback()
            raise HTTPException(status_code=404, detail="Team or membership not found")
        db.commit()
        return updated
    except PermissionError as exc:
        db.rollback()
        raise HTTPException(status_code=403, detail=str(exc)) from exc
    except ValueError as exc:
        db.rollback()
        raise HTTPException(status_code=400, detail=str(exc)) from exc


@router.delete(
    "/{team_id}/members/{membership_id}",
    status_code=status.HTTP_204_NO_CONTENT,
)
def remove_team_member_endpoint(
    team_id: UUID,
    membership_id: UUID,
    current_user: User = Depends(get_current_user),
    db: Session = Depends(get_db),
):
    try:
        removed = remove_team_member(
            db,
            team_id=team_id,
            membership_id=membership_id,
            acting_user_id=current_user.id,
        )
        if not removed:
            db.rollback()
            raise HTTPException(status_code=404, detail="Team or membership not found")
        db.commit()
        return Response(status_code=status.HTTP_204_NO_CONTENT)
    except PermissionError as exc:
        db.rollback()
        raise HTTPException(status_code=403, detail=str(exc)) from exc
    except ValueError as exc:
        db.rollback()
        raise HTTPException(status_code=400, detail=str(exc)) from exc
