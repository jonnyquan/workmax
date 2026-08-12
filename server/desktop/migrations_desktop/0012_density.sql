-- 0012: how tightly the window packs.
--
-- A second column on 0010's table rather than a table of its own, and rather
-- than a column somewhere else: 0010 said "the next machine-level UI
-- preference has an obvious home", and this is that preference arriving. It is
-- machine-scoped for the same reason appearance is — the two local identities
-- on a machine are one pair of eyes, and a layout that re-flowed when you
-- switched identity would be a bug report rather than a feature.
--
-- It rides the same row, and therefore the same read, as appearance. That is
-- the load-bearing part: the shell resolves this while it serves index.html so
-- the first frame is already the right density, and a preference that needed a
-- second round trip would either delay that frame or arrive after it as a
-- visible re-flow. One row, one query, one answer.
--
-- 'standard' is the default and is expressed in the page as the ABSENCE of
-- data-density, exactly as 'system' is the absence of data-theme: the shipped
-- stylesheet already IS the standard answer, so the default costs no markup
-- and cannot be got wrong by a shell that failed to read the database.
--
-- The CHECK is here as well as in Go because this column's value ends up in an
-- attribute on <html>. A vocabulary that is only enforced on the way in is one
-- a hand-edited database can walk straight past.

ALTER TABLE w_desktop_ui_preference
    ADD COLUMN density TEXT NOT NULL DEFAULT 'standard'
    CHECK (density IN ('compact', 'standard', 'comfortable'));

UPDATE w_desktop_ui_preference SET density = 'standard' WHERE density IS NULL;
