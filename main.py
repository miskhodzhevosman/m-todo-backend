from fastapi import FastAPI, HTTPException, status
from typing import List

from database import get_db, init_db
from schemas import TodoCreate, TodoUpdate, TodoOut

from fastapi.middleware.cors import CORSMiddleware


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


def row_to_todo(row) -> dict:
    return {
        "id": row["id"],
        "title": row["title"],
        "description": row["description"],
        "completed": bool(row["completed"]),
        "created_at": row["created_at"],
    }


# ---------- CREATE ----------
@app.post("/todos", response_model=TodoOut, status_code=status.HTTP_201_CREATED)
def create_todo(payload: TodoCreate):
    with get_db() as conn:
        cur = conn.execute(
            "INSERT INTO todos (title, description) VALUES (?, ?)",
            (payload.title, payload.description),
        )
        todo_id = cur.lastrowid
        row = conn.execute("SELECT * FROM todos WHERE id = ?", (todo_id,)).fetchone()
    return row_to_todo(row)


# ---------- READ ALL ----------
@app.get("/todos", response_model=List[TodoOut])
def list_todos(completed: bool | None = None):
    with get_db() as conn:
        if completed is None:
            rows = conn.execute("SELECT * FROM todos ORDER BY id DESC").fetchall()
        else:
            rows = conn.execute(
                "SELECT * FROM todos WHERE completed = ? ORDER BY id DESC",
                (int(completed),),
            ).fetchall()
    return [row_to_todo(r) for r in rows]


# ---------- READ ONE ----------
@app.get("/todos/{todo_id}", response_model=TodoOut)
def get_todo(todo_id: int):
    with get_db() as conn:
        row = conn.execute("SELECT * FROM todos WHERE id = ?", (todo_id,)).fetchone()
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

    fields = ", ".join(f"{k} = ?" for k in data)
    values = list(data.values()) + [todo_id]

    with get_db() as conn:
        cur = conn.execute(f"UPDATE todos SET {fields} WHERE id = ?", values)
        if cur.rowcount == 0:
            raise HTTPException(status_code=404, detail="Todo not found")
        row = conn.execute("SELECT * FROM todos WHERE id = ?", (todo_id,)).fetchone()
    return row_to_todo(row)


# ---------- DELETE ----------
@app.delete("/todos/{todo_id}", status_code=status.HTTP_204_NO_CONTENT)
def delete_todo(todo_id: int):
    with get_db() as conn:
        cur = conn.execute("DELETE FROM todos WHERE id = ?", (todo_id,))
        if cur.rowcount == 0:
            raise HTTPException(status_code=404, detail="Todo not found")
    return None
