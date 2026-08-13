-- 0014: which mind produced a cached answer.
--
-- 0013 recorded the engine and the model. Two minds can share both — that is
-- the ordinary case, since a mind's usual difference is its memory and its
-- role hint — so an answer's model does not identify the mind that shaped it,
-- and the transcript could not tell two of them apart.
--
-- The NAME, not the id. This column is read by a person under an answer and a
-- uuid tells them nothing; it also means a mind that is renamed leaves older
-- answers naming what it was called when they were written, which is what a
-- record is supposed to do rather than a bug in it.
--
-- Empty on every row written before this, and on any turn with no mind chosen.
-- The renderer says nothing rather than guessing, exactly as it does for the
-- other two.

ALTER TABLE w_workagent_message ADD COLUMN agent_mind TEXT NOT NULL DEFAULT '';
