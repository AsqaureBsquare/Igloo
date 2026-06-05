resource "aws_dynamodb_table" "users" {
  name         = "igloo-users"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "id"

  attribute {
    name = "id"
    type = "S"
  }

  attribute {
    name = "email"
    type = "S"
  }

  global_secondary_index {
    name            = "email-index"
    hash_key        = "email"
    projection_type = "ALL"
  }
}

resource "aws_dynamodb_table" "workspaces" {
  name         = "igloo-workspaces"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "id"

  attribute {
    name = "id"
    type = "S"
  }

  attribute {
    name = "user_id"
    type = "S"
  }

  global_secondary_index {
    name            = "user-index"
    hash_key        = "user_id"
    projection_type = "ALL"
  }
}

resource "aws_dynamodb_table" "templates" {
  name         = "igloo-templates"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "id"

  attribute {
    name = "id"
    type = "S"
  }
}

resource "aws_dynamodb_table" "provisioner_keys" {
  name         = "igloo-provisioner-keys"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "id"

  attribute {
    name = "id"
    type = "S"
  }

  attribute {
    name = "token_hash"
    type = "S"
  }

  global_secondary_index {
    name            = "token-hash-index"
    hash_key        = "token_hash"
    projection_type = "ALL"
  }
}

resource "aws_dynamodb_table" "jobs" {
  name         = "igloo-ws-jobs"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "id"

  attribute {
    name = "id"
    type = "S"
  }

  attribute {
    name = "status"
    type = "S"
  }

  attribute {
    name = "created_at"
    type = "S"
  }

  global_secondary_index {
    name            = "status-created_at-index"
    hash_key        = "status"
    range_key       = "created_at"
    projection_type = "ALL"
  }

  ttl {
    attribute_name = "ttl"
    enabled        = true
  }
}

# ─── Organisations & Teams (single-table design) ──────────────────────────────
# pk / sk patterns:
#   Org:        pk=ORG#<id>      sk=METADATA
#   OrgMember:  pk=ORG#<orgId>   sk=MEMBER#<userId>   gsi1pk=USER#<userId>  gsi1sk=ORG#<orgId>
#   Team:       pk=TEAM#<id>     sk=METADATA           gsi1pk=ORG#<orgId>    gsi1sk=TEAM#<id>
#   TeamMember: pk=TEAM#<teamId> sk=MEMBER#<userId>   gsi1pk=USER#<userId>  gsi1sk=TEAM#<teamId>

resource "aws_dynamodb_table" "entities" {
  name         = "igloo-entities"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "pk"
  range_key    = "sk"

  attribute {
    name = "pk"
    type = "S"
  }
  attribute {
    name = "sk"
    type = "S"
  }
  attribute {
    name = "slug"
    type = "S"
  }
  attribute {
    name = "gsi1pk"
    type = "S"
  }
  attribute {
    name = "gsi1sk"
    type = "S"
  }

  global_secondary_index {
    name            = "slug-index"
    hash_key        = "slug"
    projection_type = "ALL"
  }

  global_secondary_index {
    name            = "gsi1-index"
    hash_key        = "gsi1pk"
    range_key       = "gsi1sk"
    projection_type = "ALL"
  }
}
