from dataclasses import dataclass, field
from datetime import datetime, timezone
from enum import Enum
from typing import Optional


class WorkspaceStatus(str, Enum):
    CREATING   = "creating"
    RUNNING    = "running"
    SLEEPING   = "sleeping"
    WAKING     = "waking"
    DESTROYING = "destroying"
    ERROR      = "error"


class WorkspaceType(str, Enum):
    CONTAINER = "container"
    VM        = "vm"
    GPU       = "gpu"


def _now() -> str:
    return datetime.now(timezone.utc).isoformat()


@dataclass
class Workspace:
    workspace_id:   str
    user_id:        str
    status:         WorkspaceStatus
    workspace_type: WorkspaceType
    task_arn:       Optional[str] = None
    volume_id:      Optional[str] = None
    az:             Optional[str] = None
    subnet_id:      Optional[str] = None
    created_at:     str = field(default_factory=_now)
    last_activity:  str = field(default_factory=_now)
    template:       str = "default"

    def to_dynamo(self) -> dict:
        return {
            "workspace_id":   self.workspace_id,
            "user_id":        self.user_id,
            "status":         self.status.value,
            "workspace_type": self.workspace_type.value,
            "task_arn":       self.task_arn or "",
            "volume_id":      self.volume_id or "",
            "az":             self.az or "",
            "subnet_id":      self.subnet_id or "",
            "created_at":     self.created_at,
            "last_activity":  self.last_activity,
            "template":       self.template,
        }

    @classmethod
    def from_dynamo(cls, item: dict) -> "Workspace":
        return cls(
            workspace_id   = item["workspace_id"],
            user_id        = item["user_id"],
            status         = WorkspaceStatus(item["status"]),
            workspace_type = WorkspaceType(item["workspace_type"]),
            task_arn       = item.get("task_arn") or None,
            volume_id      = item.get("volume_id") or None,
            az             = item.get("az") or None,
            subnet_id      = item.get("subnet_id") or None,
            created_at     = item.get("created_at", _now()),
            last_activity  = item.get("last_activity", _now()),
            template       = item.get("template", "default"),
        )
