-- D225: published tools are read from their real ToolUse, not display caches.
-- The migration hook proves same-call input/output (including exact historical
-- clipping) before clearing redundant tool_json. Unknown or distinct data stays.
SELECT 1;
