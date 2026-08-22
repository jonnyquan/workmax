-- 0016: which language the application chrome speaks.
--
-- The third column on 0010's table, for the reason 0012 already established:
-- this is a machine-level UI preference, and it rides the same row — and
-- therefore the same single read — that the shell already performs while it
-- serves index.html. The first frame arrives in the right language with no
-- extra round trip.
--
-- Machine-scoped, like appearance and density: the two local identities on a
-- machine are one pair of eyes, and a window that changed language when you
-- switched identity would be a bug report rather than a feature.
--
-- 'system' is the default and means "ask the webview": the document request
-- carries an Accept-Language header that already reflects the OS setting, and
-- the shell resolves it there. That is why the default is a word rather than a
-- locale code — a stored 'en' would mean "the user chose English", which is a
-- different fact from "nobody has chosen", and only the second one should
-- follow the machine when its language changes.
--
-- Two locales, not eighteen. The Server's own resolver ships eighteen, but the
-- desktop chrome is a different body of copy that has to be written and kept
-- correct here; this column grows when a locale actually ships, not before.
--
-- The CHECK is here as well as in Go for 0012's reason: this column's value
-- ends up in the lang attribute on <html>. A vocabulary that is only enforced
-- on the way in is one a hand-edited database can walk straight past.

ALTER TABLE w_desktop_ui_preference
    ADD COLUMN language TEXT NOT NULL DEFAULT 'system'
    CHECK (language IN ('system', 'en', 'zh'));

UPDATE w_desktop_ui_preference SET language = 'system' WHERE language IS NULL;
