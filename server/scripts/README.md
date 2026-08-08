# Skills Migration Scripts

This directory contains scripts to import skills from the filesystem into the database.

## Migration Order

Run these SQL migrations in order:

1. `20241224_replace_agent_mode_with_skills.sql` - Adds skills field to messages, removes agent_mode
2. `20241224_create_skill_tables.sql` - Creates skill tables and categories
3. `20241224_import_skills.sql` - Imports 107 skills from agent_workspace_template/skills

## Scripts

### generate_skill_migration.py

Python script to regenerate the `20241224_import_skills.sql` migration file from the latest skill files.

```bash
cd server/scripts
python3 generate_skill_migration.py > ../migrations/20241224_import_skills.sql
```

### import_skills.go

Go script for direct database import (alternative to SQL migration).

```bash
cd server/scripts
go run import_skills.go 'user:pass@tcp(localhost:3306)/dbname?charset=utf8mb4&parseTime=True' ../agent_workspace_template/skills
```

### generate_skill_inserts.sh

Shell script to generate SQL INSERT statements (simpler alternative).

```bash
cd server/scripts
./generate_skill_inserts.sh ../agent_workspace_template/skills > skill_inserts.sql
```

## Skill File Structure

Skills are stored in `server/agent_workspace_template/skills/{category}/{skill-name}/SKILL.md`

Each SKILL.md has YAML frontmatter:
```yaml
---
name: skill-name
description: Description of what the skill does
license: Optional license info
---

# Skill Title

Skill content/prompt in Markdown...
```

## Categories

The following categories are pre-seeded:

| Slug | Name | Icon | Color |
|------|------|------|-------|
| business-marketing | Business & Marketing | Briefcase | blue |
| creative-design | Creative & Design | Palette | purple |
| database | Database | Database | green |
| development | Development | Code | orange |
| document-processing | Document Processing | FileText | red |
| enterprise-communication | Enterprise & Compliance | Building | cyan |
| media | Media | Image | pink |
| productivity | Productivity | Zap | yellow |
| utilities | Utilities | Wrench | gray |
