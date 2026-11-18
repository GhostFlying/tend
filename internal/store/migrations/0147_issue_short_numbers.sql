CREATE TABLE issue_short_numbers (
 number INTEGER PRIMARY KEY AUTOINCREMENT CHECK(number > 0),
 issue_id TEXT NOT NULL UNIQUE REFERENCES work_items(work_item_id)
) STRICT;

INSERT INTO issue_short_numbers(issue_id)
SELECT work_item_id FROM work_items ORDER BY created_at, work_item_id;

CREATE TRIGGER work_item_allocate_short_number AFTER INSERT ON work_items
BEGIN
 INSERT INTO issue_short_numbers(issue_id) VALUES(NEW.work_item_id);
END;

CREATE TRIGGER issue_short_number_immutable_update BEFORE UPDATE ON issue_short_numbers
BEGIN SELECT RAISE(ABORT, 'Issue short numbers are immutable'); END;

CREATE TRIGGER issue_short_number_immutable_delete BEFORE DELETE ON issue_short_numbers
BEGIN SELECT RAISE(ABORT, 'Issue short numbers are immutable'); END;
