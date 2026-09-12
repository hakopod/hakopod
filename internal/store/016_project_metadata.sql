ALTER TABLE projects ADD COLUMN display_name text NOT NULL DEFAULT '' CHECK (char_length(display_name) <= 80);
ALTER TABLE projects ADD COLUMN description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 1000);
