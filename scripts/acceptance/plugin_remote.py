#!/usr/bin/env python3
"""Real public-source acceptance; installs only in a disposable home, never a session.

Uses an inspected historical frontend-design commit from Anthropic's public
marketplace as the installed baseline. The product discovers the current remote
commit, refreshes through Claude, freezes the source, and updates the plugin.
"""
import argparse
import json
from pathlib import Path
import subprocess
import tempfile

SOURCE = 'https://github.com/anthropics/claude-plugins-official.git'
BASELINE = 'd029127f7d29bdb8fd8902ac34dd7d5c8ba92b6e'
BATCH_BASELINE = '7ee24ab71fbba2dcb3f780fcd785757a3c28e8d2'
PROFILE = '(version 1)(allow default)(deny network*)'


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--cli', required=True, type=Path)
    parser.add_argument('--claude', required=True, type=Path)
    parser.add_argument('--batch', action='store_true', help='verify two official plugins share one refresh')
    args = parser.parse_args()
    cli, claude = args.cli.resolve(strict=True), args.claude.resolve(strict=True)
    baseline = BATCH_BASELINE if args.batch else BASELINE
    names = ['frontend-design', 'playground'] if args.batch else ['frontend-design']
    plugins = [name + '@claude-plugins-official' for name in names]
    with tempfile.TemporaryDirectory(prefix='keepupdated-plugin-public-') as temp:
        home = Path(temp).resolve() / 'home'
        home.mkdir()
        config = home / '.claude'
        market = config / 'plugins/marketplaces/claude-plugins-official'
        market.mkdir(parents=True)
        environment = {
            'HOME': str(home), 'CLAUDE_CONFIG_DIR': str(config), 'PATH': '/usr/bin:/bin',
            'LC_ALL': 'C', 'DISABLE_AUTOUPDATER': '1',
            'CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC': '1',
            'GIT_CONFIG_NOSYSTEM': '1', 'GIT_CONFIG_GLOBAL': '/dev/null',
            'GIT_TERMINAL_PROMPT': '0', 'GIT_ALLOW_PROTOCOL': 'https',
        }

        def run(command, allowed=(0,)):
            result = subprocess.run([str(x) for x in command], cwd=home, env=environment,
                                    stdin=subprocess.DEVNULL, capture_output=True, text=True,
                                    timeout=180)
            if result.returncode not in allowed:
                raise RuntimeError(f'{Path(command[0]).name} failed ({result.returncode}): {result.stderr}')
            return result.stdout

        def git(*command):
            return run(['/usr/bin/git', '-c', 'credential.helper=', '-c', 'core.hooksPath=/dev/null',
                        '-C', market, *command]).strip()

        def native(*command):
            return run(['/usr/bin/sandbox-exec', '-p', PROFILE, claude, 'plugin', *command])

        git('init', '-b', 'main')
        git('remote', 'add', 'origin', SOURCE)
        git('fetch', '--depth=1', 'origin', baseline)
        git('reset', '--hard', 'FETCH_HEAD')
        git('update-ref', 'refs/remotes/origin/main', 'HEAD')
        # Inspect the immutable baseline before allowing the native installer to read it.
        for name in names:
            source_plugin = market / 'plugins' / name
            manifest = json.loads((source_plugin / '.claude-plugin/plugin.json').read_text())
            assert manifest['name'] == name and 'version' not in manifest
            assert set(manifest) <= {'name', 'description', 'author'}
            for path in source_plugin.rglob('*'):
                assert not path.is_symlink()
                if path.is_file():
                    relative = str(path.relative_to(source_plugin))
                    assert relative == '.claude-plugin/plugin.json' or path.suffix == '.md' or path.name in ('LICENSE', 'LICENSE.txt')
                    assert path.stat().st_mode & 0o111 == 0
        known = {'claude-plugins-official': {
            'source': {'source': 'git', 'url': SOURCE},
            'installLocation': str(market), 'lastUpdated': '2026-10-19T00:00:00Z',
        }}
        (config / 'plugins/known_marketplaces.json').write_text(json.dumps(known))
        (config / 'settings.json').write_text('{}')
        for plugin in plugins:
            installed = json.loads(native('install', plugin, '--scope', 'user', '--json').strip().splitlines()[-1])
            assert installed['outcome'] == 'ok'
            records = json.loads((config / 'plugins/installed_plugins.json').read_text())['plugins'][plugin]
            assert len(records) == 1 and records[0]['version'] == baseline[:12]
        manager_link = home / '.local/bin/claude'
        manager_link.parent.mkdir(parents=True)
        manager_link.symlink_to(claude)
        inventory = json.loads(run([cli, 'scan', '--format', 'json'], allowed=(0, 1)))
        skill_ids = [x['id'] for x in inventory['items'] if x.get('plugin', {}).get('id') in plugins]
        assert len(skill_ids) == len(plugins)
        selection = [value for item_id in skill_ids for value in ('--id', item_id)]
        plan = json.loads(run([cli, 'plan', '--action', 'update', *selection]))
        ready = [item for item in plan['items'] if item['status'] == 'ready']
        assert len(ready) == len(plugins), [item['reason'] for item in plan['items']]
        target = ready[0]['plugin_update']['commit']
        assert target != baseline
        for item in ready:
            assert item['plugin_update']['commit'] == target and item['target_version'] == target[:12]
            assert item['plugin_update']['refresh_marketplace']
        assert git('rev-parse', 'HEAD') == baseline, 'preview changed the original marketplace'
        plan_file = home / 'plan.json'
        plan_file.write_text(json.dumps(plan))
        applied = json.loads(run([cli, 'apply', '--plan', plan_file, '--confirm', plan['plan_id']], allowed=(0, 1)))
        results = [r for r in applied['results'] if r['status'] == 'updated']
        assert len(results) == len(plugins), applied['results']
        assert all(r['reload_required'] for r in results)
        effects = [r['marketplace_refresh'] for r in results if r.get('marketplace_refresh')]
        assert len(effects) == 1, 'shared marketplace was refreshed more than once'
        effect = effects[0]
        assert effect['attempted'] and effect['command_succeeded'] and effect['state_known'] and effect['changed']
        assert effect['before_commit'] == baseline and effect['after_commit'] == target
        listed = json.loads(native('list', '--json'))
        for plugin, name in zip(plugins, names):
            records = json.loads((config / 'plugins/installed_plugins.json').read_text())['plugins'][plugin]
            assert len(records) == 1 and records[0]['version'] == target[:12] and records[0]['gitCommitSha'] == target
            current = [x for x in listed if x['id'] == plugin]
            assert len(current) == 1 and current[0]['version'] == target[:12] and current[0]['enabled']
            assert not current[0].get('readFromFolder')
            result = next(r for r in results if r['name'] == plugin)
            assert (Path(result['backup_path']) / 'plugin/skills' / name / 'SKILL.md').is_file()
        source_result = next(r for r in results if r.get('marketplace_refresh'))
        assert (Path(source_result['backup_path']) / 'marketplace-before.json').is_file()
        print('PASS: real public GitHub discovery, read-only preview, native marketplace refresh, '
              'approved-commit check, network-isolated native plugin update, backup, and native version readback.')
        print(f'Official {", ".join(names)}: {baseline[:12]} -> {target[:12]}; one marketplace refresh; sessions still need reload.')


if __name__ == '__main__':
    main()
