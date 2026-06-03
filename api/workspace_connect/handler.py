import json
import logging
from models import WorkspaceStatus
from providers.aws import AWSProvider
import db

logger = logging.getLogger(__name__)
logger.setLevel(logging.INFO)

provider = AWSProvider()


def lambda_handler(event, context):
    try:
        workspace_id = event["pathParameters"]["workspace_id"]
        user_id      = event["requestContext"]["authorizer"]["user_id"]

        workspace = db.get_workspace(workspace_id)
        if not workspace:
            return {"statusCode": 404, "body": json.dumps({"error": "workspace not found"})}

        if workspace.user_id != user_id:
            return {"statusCode": 403, "body": json.dumps({"error": "forbidden"})}

        if workspace.status != WorkspaceStatus.RUNNING:
            return {
                "statusCode": 409,
                "body": json.dumps({
                    "error":  f"workspace is {workspace.status.value}",
                    "hint":   "run `igloo wake` first" if workspace.status == WorkspaceStatus.SLEEPING else None,
                }),
            }

        connection = provider.get_connection(workspace)

        # Bump last_activity so idle_monitor doesn't sleep an active workspace
        db.update_workspace(workspace_id)

        logger.info("Connected user %s to workspace %s", user_id, workspace_id)
        return {
            "statusCode": 200,
            "headers":    {"Content-Type": "application/json"},
            "body":       json.dumps(connection),  # {ws_url, ticket, workspace_id}
        }

    except ValueError as e:
        return {"statusCode": 400, "body": json.dumps({"error": str(e)})}
    except Exception as e:
        logger.exception("workspace_connect failed")
        return {"statusCode": 500, "body": json.dumps({"error": str(e)})}
