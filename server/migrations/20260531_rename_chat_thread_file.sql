-- Rename w_chat_thread_files → w_chat_thread_file. The table holds
-- one-row-per-file (each row carries fileName / filePath / fileSize),
-- and the only other neighbour of the same family — w_chat_thread,
-- w_chat_message — is already singular. The plural was an outlier.

RENAME TABLE `w_chat_thread_files` TO `w_chat_thread_file`;
