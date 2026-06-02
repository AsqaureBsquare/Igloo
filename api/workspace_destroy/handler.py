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
        keep_volume  = event.get("queryStringParameters", {}) or {}
        keep_volume  = keep_volume.get("keep_volume", "false").lower() == "true"

        workspace = db.get_workspace(workspace_id)
        if not workspace:
            return {"statusCode": 404, "body": json.dumps({"error": "workspace not found"})}

        if workspace.user_id != user_id:
            return {"statusCode": 403, "body": json.dumps({"error": "forbidden"})}

        db.update_workspace(workspace_id, status=WorkspaceStatus.DESTROYING.value)
        provider.destroy_workspace(workspace, keep_volume=keep_volume)
        db.delete_workspace(workspace_id)

        logger.info("Destroyed workspace %s (keep_volume=%s)", workspace_id, keep_volume)
        return {
            "statusCode": 200,
            "body": json.dumps({"workspace_id": workspace_id, "destroyed": True}),
        }

    except Exception as e:
        logger.exception("workspace_destroy failed")
        return {"statusCode": 500, "body": json.dumps({"error": "internal error"})}
