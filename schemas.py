from pydantic import BaseModel, Field
from typing import Optional, List
from datetime import datetime
from constants import Urgency


# ---------- Categories ----------
class CategoryCreate(BaseModel):
    name: str = Field(..., min_length=1, max_length=50)
    color: str = Field("#6366f1", pattern=r"^#[0-9a-fA-F]{6}$")


class CategoryUpdate(BaseModel):
    name: Optional[str] = Field(None, min_length=1, max_length=50)
    color: Optional[str] = Field(None, pattern=r"^#[0-9a-fA-F]{6}$")


class CategoryOut(BaseModel):
    id: int
    name: str
    color: str
    created_at: str


# ---------- Todos ----------
class TodoCreate(BaseModel):
    title: str = Field(..., min_length=1, max_length=200)
    description: Optional[str] = None
    urgency: Urgency = Urgency.MEDIUM
    due_at: Optional[datetime] = None
    category_id: Optional[int] = None


class TodoUpdate(BaseModel):
    title: Optional[str] = Field(None, min_length=1, max_length=200)
    description: Optional[str] = None
    completed: Optional[bool] = None
    urgency: Optional[Urgency] = None
    due_at: Optional[datetime] = None
    category_id: Optional[int] = None


class TodoOut(BaseModel):
    id: int
    title: str
    description: Optional[str]
    completed: bool
    urgency: Urgency
    due_at: Optional[str]
    created_at: str
    category_id: Optional[int]
    category: Optional[CategoryOut] = None  # развёрнутая категория
