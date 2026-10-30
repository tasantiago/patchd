-- Zero denotes unlimited uses. A zero Go timestamp denotes no expiration.
ALTER TABLE enrollment_tokens DROP CONSTRAINT enrollment_tokens_max_uses_check;
ALTER TABLE enrollment_tokens ADD CHECK (max_uses >= 0);
