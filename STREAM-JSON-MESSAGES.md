
# Raw event types received in chat, check how to handle in ui or backend:

#1 {
  "type": "rate_limit_event",
  "rate_limit_info": {
    "status": "allowed",
    "resetsAt": 1775358000,
    "rateLimitType": "five_hour",
    "overageStatus": "rejected",
    "overageDisabledReason": "org_level_disabled",
    "isUsingOverage": false
  },
  "uuid": "ec2eca36-daba-494e-ac2f-1d4c5f3d270f",
  "session_id": "67817478-892c-4cdb-a26e-805530bc2869"
}

#2 {
  "type": "assistant",
  "message": {
    "model": "claude-haiku-4-5-20251001",
    "id": "msg_01UgRiRnsMwTrrHq1oAQLGeB",
    "type": "message",
    "role": "assistant",
    "content": [
      {
        "type": "thinking",
        "thinking": "The user just said \"hola\" which is Spanish for \"hello\". This is a simple greeting. I should respond warmly in a similar manner.",
        "signature": "EsUCCmMIDBgCKkAYNfvwyC0qKAZI+WYl+fdBYxEh+njBP2Af9tuJlK8N7zELr5c6czIdlmIWc8LjMavzrHeGl7dJpmZLihPDFn2CMhljbGF1ZGUtaGFpa3UtNC01LTIwMjUxMDAxOAASDPG1CkqDsijfkI/ugxoM+nAZX2N8K8LCqqaJIjDjr75RZtq9HnNlCgs1zpeSgaTgDov3OIuekW+aQFj1BVjY+d2LjPKczJUDeZgxBtAqjwG3Rsxrzw/do/WtJi+RQjYdZngtY7R+WeeEaIYrfTEqKBYPcYLD42GKaYSP32gNGDz4GV2LUOy6pKjyebU5adYq9/svNnlircK3OiUxnbN6JI+UYOC9r3B9AVo+4XTFzveh3fDfOpO+A7vHM2N2eXKwSYlwVGtRnbboK3r6J72aA/x/+7eNnPWp/rtZnsEL9hgB"
      }
    ],
    "stop_reason": null,
    "stop_sequence": null,
    "stop_details": null,
    "usage": {
      "input_tokens": 10,
      "cache_creation_input_tokens": 1137,
      "cache_read_input_tokens": 25035,
      "cache_creation": {
        "ephemeral_5m_input_tokens": 0,
        "ephemeral_1h_input_tokens": 1137
      },
      "output_tokens": 3,
      "service_tier": "standard",
      "inference_geo": "not_available"
    },
    "context_management": null
  },
  "parent_tool_use_id": null,
  "session_id": "773195b2-8c15-46bb-b200-9cd4cfb2a140",
  "uuid": "5604621b-24ce-4ae4-968c-505b89dae82c"
}

#3 {
  "type": "assistant",
  "message": {
    "model": "claude-haiku-4-5-20251001",
    "id": "msg_01UgRiRnsMwTrrHq1oAQLGeB",
    "type": "message",
    "role": "assistant",
    "content": [
      {
        "type": "text",
        "text": "¡Hola! 👋 \n\nHow can I help you with your coding or software engineering tasks today?"
      }
    ],
    "stop_reason": null,
    "stop_sequence": null,
    "stop_details": null,
    "usage": {
      "input_tokens": 10,
      "cache_creation_input_tokens": 1137,
      "cache_read_input_tokens": 25035,
      "cache_creation": {
        "ephemeral_5m_input_tokens": 0,
        "ephemeral_1h_input_tokens": 1137
      },
      "output_tokens": 3,
      "service_tier": "standard",
      "inference_geo": "not_available"
    },
    "context_management": null
  },
  "parent_tool_use_id": null,
  "session_id": "773195b2-8c15-46bb-b200-9cd4cfb2a140",
  "uuid": "753937a9-2300-4af7-8aeb-b85253ed01bd"
}