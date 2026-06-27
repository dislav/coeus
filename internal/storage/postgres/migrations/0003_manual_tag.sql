-- Seeds the manual-entry tag used to mark hand-authored questions (spec §3.4).
-- Not strictly required (linkTag upserts by name) but keeps the tags table tidy
-- and makes manual-entry visible to the expert tag filter.
INSERT INTO tags (name) VALUES ('manual-entry') ON CONFLICT DO NOTHING;
