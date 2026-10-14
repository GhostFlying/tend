-- Structured details of a recorded failure, such as the original request of
-- a duplicate effect, so a retry returns exactly the same error.
ALTER TABLE request_records ADD COLUMN error_details TEXT;
