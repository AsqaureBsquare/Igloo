from abc import ABC, abstractmethod
from models import Workspace, WorkspaceStatus, WorkspaceType


class WorkspaceProvider(ABC):
    """
    Every cloud backend implements this interface.
    Swap providers without touching the Lambda handlers or CLI.
    """

    @abstractmethod
    def create_workspace(
        self,
        user_id: str,
        workspace_type: WorkspaceType,
        template: str = "default",
    ) -> Workspace:
        """Provision compute + persistent disk. Returns workspace in CREATING state."""

    @abstractmethod
    def sleep_workspace(self, workspace: Workspace) -> None:
        """Stop compute. Disk must persist. Cost drops to ~$0."""

    @abstractmethod
    def wake_workspace(self, workspace: Workspace) -> str:
        """Restart compute, re-attach same disk. Returns new task/instance ARN."""

    @abstractmethod
    def destroy_workspace(self, workspace: Workspace, keep_volume: bool = False) -> None:
        """Tear down all resources. Optionally keep disk as backup."""

    @abstractmethod
    def get_connection(self, workspace: Workspace) -> dict:
        """Return connection params the CLI uses to open a terminal session."""

    @abstractmethod
    def get_status(self, workspace: Workspace) -> WorkspaceStatus:
        """Live status from the cloud provider (not from DynamoDB cache)."""
