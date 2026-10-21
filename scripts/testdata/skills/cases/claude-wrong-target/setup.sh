# .claude/skills is a symlink, but to a folder other than .agents/skills.
rm .claude/skills
ln -s ../.agents .claude/skills
