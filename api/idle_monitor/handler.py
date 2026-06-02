"""
EventBridge scheduled Lambda.
Runs every 5 minutes. Sleeps workspaces idle for > IDLE_TIMEOUT_MINUTES.
"""
import os
import logging
from datetime import datetime, timezone, timedelta
from models import WorkspaceStatus
from providers.aws import AWSProvider
import db

logger = logging.getLogger(__name__)
logger.setLevel(logging.INFO)

IDLE_TIMEOUT = int(os.environ.get("IDLE_TIMEOUT_MINUTES", "10"))

provider = AWSProvider()


def lambda_handler(event, context):
    now      = datetime.now(timezone.utc)
    cutoff   = now - timedelta(minutes=IDLE_TIMEOUT)
    slept    = []
    errors   = []

    running = db.list_running()
    logger.info("Checking %d running workspaces (idle timeout=%dm)", len(running), IDLE_TIMEOUT)

    for ws in running:
        try:
            last = datetime.fromisoformat(ws.last_activity)
            if last.tzinfo is None:
                last = last.replace(tzinfo=timezone.utc)

            if last < cutoff:
                idle_mins = (now - last).seconds // 60
                logger.info(
                    "Sleeping workspace %s (user=%s, idle=%dm)",
                    ws.workspace_id, ws.user_id, idle_mins,
                )
                db.update_workspace(ws.workspace_id, status=WorkspaceStatus.SLEEPING.value)
                provider.sleep_workspace(ws)
                slept.append(ws.workspace_id)

        except Exception as e:
            logger.exception("Failed to process workspace %s", ws.workspace_id)
            errors.append(ws.workspace_id)

    logger.info("idle_monitor done: slept=%d errors=%d", len(slept), len(errors))
    return {"slept": slept, "errors": errors}
