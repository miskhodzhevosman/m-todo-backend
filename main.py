from fastapi import FastAPI, HTTPException, status, Query
from fastapi.middleware.cors import CORSMiddleware
from typing import List, Optional

from database import get_db, init_db, create_indexes

from migrations import run_migrations
from constants import Urgency
from schemas import (
    TodoCreate, TodoUpdate, TodoOut,
    CategoryCreate, CategoryUpdate, CategoryOut,
)

app = FastAPI(title="Todo API")

app.add_middleware(
    CORSMiddleware,
    allow_origins=["http://localhost:5173"],
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)


@app.on_event("startup")
def on_startup():
    init_db()
    run_migrations()
    create_indexes()
# ---------- helpers ----------
TODO_SELECT = """
    SELECT
        t.*,
        c.name       AS cat_name,
        c.color      AS cat_color,
        c.created_at AS cat_created_at
    FROM todos t
    LEFT JOIN categories c ON c.id = t.category_id
"""


def row_to_category(row) -> dict:
    return {
        "id": row["id"],
        "name": row["name"],
        "color": row["color"],
        "created_at": row["created_at"],
    }


def row_to_todo(row) -> dict:
    cat = None
    if "cat_name" in row.keys() and row["cat_name"] is not None:
        cat = {
            "id": row["category_id"],
            "name": row["cat_name"],
            "color": row["cat_color"],
            "created_at": row["cat_created_at"],
        }
    return {
        "id": row["id"],
        "title": row["title"],
        "description": row["description"],
        "completed": bool(row["completed"]),
        "urgency": row["urgency"],
        "due_at": row["due_at"],
        "created_at": row["created_at"],
        "category_id": row["category_id"],
        "category": cat,
    }


def _check_category_exists(conn, category_id: Optional[int]):
    if category_id is None:
        return
    row = conn.execute(
        "SELECT 1 FROM categories WHERE id = ?", (category_id,)
    ).fetchone()
    if not row:
        raise HTTPException(status_code=400, detail="Category not found")


# ==================================================
# CATEGORIES
# ==================================================

@app.get("/categories", response_model=List[CategoryOut])
def list_categories():
    with get_db() as conn:
        rows = conn.execute(
            "SELECT * FROM categories ORDER BY name COLLATE NOCASE"
        ).fetchall()
    return [row_to_category(r) for r in rows]


@app.post("/categories", response_model=CategoryOut, status_code=status.HTTP_201_CREATED)
def create_category(payload: CategoryCreate):
    with get_db() as conn:
        try:
            cur = conn.execute(
                "INSERT INTO categories (name, color) VALUES (?, ?)",
                (payload.name.strip(), payload.color),
            )
        except Exception as e:
            if "UNIQUE" in str(e):
                raise HTTPException(status_code=409, detail="Category already exists")
            raise
        row = conn.execute(
            "SELECT * FROM categories WHERE id = ?", (cur.lastrowid,)
        ).fetchone()
    return row_to_category(row)


@app.patch("/categories/{category_id}", response_model=CategoryOut)
def update_category(category_id: int, payload: CategoryUpdate):
    data = payload.model_dump(exclude_unset=True)
    if not data:
        raise HTTPException(status_code=400, detail="No fields to update")

    fields = ", ".join(f"{k} = ?" for k in data)
    values = list(data.values()) + [category_id]

    with get_db() as conn:
        try:
            cur = conn.execute(
                f"UPDATE categories SET {fields} WHERE id = ?", values
            )
        except Exception as e:
            if "UNIQUE" in str(e):
                raise HTTPException(status_code=409, detail="Category already exists")
            raise
        if cur.rowcount == 0:
            raise HTTPException(status_code=404, detail="Category not found")
        row = conn.execute(
            "SELECT * FROM categories WHERE id = ?", (category_id,)
        ).fetchone()
    return row_to_category(row)


@app.delete("/categories/{category_id}", status_code=status.HTTP_204_NO_CONTENT)
def delete_category(category_id: int):
    with get_db() as conn:
        cur = conn.execute("DELETE FROM categories WHERE id = ?", (category_id,))
        if cur.rowcount == 0:
            raise HTTPException(status_code=404, detail="Category not found")
    return None


# ==================================================
# TODOS
# ==================================================

# ---------- CREATE ----------
@app.post("/todos", response_model=TodoOut, status_code=status.HTTP_201_CREATED)
def create_todo(payload: TodoCreate):
    due_at = payload.due_at.isoformat() if payload.due_at else None
    with get_db() as conn:
        _check_category_exists(conn, payload.category_id)
        cur = conn.execute(
            """
            INSERT INTO todos (title, description, urgency, due_at, category_id)
            VALUES (?, ?, ?, ?, ?)
            """,
            (
                payload.title.strip(),
                payload.description,
                payload.urgency.value,
                due_at,
                payload.category_id,
            ),
        )
        todo_id = cur.lastrowid
        row = conn.execute(TODO_SELECT + " WHERE t.id = ?", (todo_id,)).fetchone()
    return row_to_todo(row)


# ---------- READ ALL ----------
@app.get("/todos", response_model=List[TodoOut])
def list_todos(
    completed: Optional[bool] = None,
    urgency: Optional[Urgency] = None,
    category_id: Optional[int] = None,
    overdue: Optional[bool] = Query(None, description="Только просроченные"),
):
    where, params = [], []

    if completed is not None:
        where.append("t.completed = ?")
        params.append(int(completed))
    if urgency is not None:
        where.append("t.urgency = ?")
        params.append(urgency.value)
    if category_id is not None:
        where.append("t.category_id = ?")
        params.append(category_id)
    if overdue:
        where.append(
            "t.due_at IS NOT NULL AND t.due_at < datetime('now') AND t.completed = 0"
        )

    sql = TODO_SELECT
    if where:
        sql += " WHERE " + " AND ".join(where)

    sql += """
        ORDER BY
            CASE WHEN t.due_at IS NULL THEN 1 ELSE 0 END,
            t.due_at ASC,
            CASE t.urgency
                WHEN 'critical' THEN 0
                WHEN 'high'     THEN 1
                WHEN 'medium'   THEN 2
                WHEN 'low'      THEN 3
                ELSE 4
            END,
            t.created_at DESC
    """

    with get_db() as conn:
        rows = conn.execute(sql, params).fetchall()
    return [row_to_todo(r) for r in rows]


# ---------- READ ONE ----------
@app.get("/todos/{todo_id}", response_model=TodoOut)
def get_todo(todo_id: int):
    with get_db() as conn:
        row = conn.execute(TODO_SELECT + " WHERE t.id = ?", (todo_id,)).fetchone()
    if not row:
        raise HTTPException(status_code=404, detail="Todo not found")
    return row_to_todo(row)


# ---------- UPDATE ----------
@app.patch("/todos/{todo_id}", response_model=TodoOut)
def update_todo(todo_id: int, payload: TodoUpdate):
    data = payload.model_dump(exclude_unset=True)
    if not data:
        raise HTTPException(status_code=400, detail="No fields to update")

    if "completed" in data:
        data["completed"] = int(data["completed"])
    if "urgency" in data and data["urgency"] is not None:
        data["urgency"] = data["urgency"].value
    if "due_at" in data and data["due_at"] is not None:
        data["due_at"] = data["due_at"].isoformat()

    fields = ", ".join(f"{k} = ?" for k in data)
    values = list(data.values()) + [todo_id]

    with get_db() as conn:
        if "category_id" in data:
            _check_category_exists(conn, data["category_id"])

        cur = conn.execute(f"UPDATE todos SET {fields} WHERE id = ?", values)
        if cur.rowcount == 0:
            raise HTTPException(status_code=404, detail="Todo not found")
        row = conn.execute(TODO_SELECT + " WHERE t.id = ?", (todo_id,)).fetchone()
    return row_to_todo(row)


# ---------- DELETE ----------
@app.delete("/todos/{todo_id}", status_code=status.HTTP_204_NO_CONTENT)
def delete_todo(todo_id: int):
    with get_db() as conn:
        cur = conn.execute("DELETE FROM todos WHERE id = ?", (todo_id,))
        if cur.rowcount == 0:
            raise HTTPException(status_code=404, detail="Todo not found")
    return None
