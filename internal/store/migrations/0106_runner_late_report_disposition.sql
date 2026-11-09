-- This is the response policy for a retired query, not a claim that its report
-- arrived. Only a real report may populate the original late_report_json.
ALTER TABLE retired_runner_delivery_queries ADD COLUMN accept_late_report INTEGER NOT NULL DEFAULT 0;
