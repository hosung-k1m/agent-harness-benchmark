#!/usr/bin/env python3
"""Write the credential-free Result schema; raw provider streams stay in /tmp."""
import json, os, sys

kind, raw_path, session_root, result_path, final_path, exit_code, elapsed, *optional = sys.argv[1:]
exit_code, elapsed = int(exit_code), float(elapsed)
stderr_path = optional[0] if optional else None

def count(obj, *keys):
    if not isinstance(obj, dict): return None
    for key in keys:
        value = obj.get(key)
        if isinstance(value, int) and not isinstance(value, bool) and value >= 0:
            return value
    return None

def usage_fields(obj):
    src = obj.get('usage', obj) if isinstance(obj, dict) else {}
    if not isinstance(src, dict): return (None, None, None, None, None)
    return (count(src, 'input_tokens', 'inputTokens', 'prompt_tokens'),
            count(src, 'cached_input_tokens', 'cachedInputTokens', 'cache_read_input_tokens', 'cacheReadTokens'),
            count(src, 'output_tokens', 'outputTokens', 'completion_tokens'),
            count(src, 'reasoning_output_tokens', 'reasoningOutputTokens', 'reasoning_tokens', 'reasoningTokens'),
            count(src, 'total_tokens', 'totalTokens'))

usage, final, config_valid = (None, None, None, None), '', True
if kind == 'codex':
    # Codex CLI 0.151.0 exposes aggregate usage only on turn.completed. Item
    # events are deliberately ignored because they can be partial or mirrored.
    try:
        for line in open(raw_path, encoding='utf-8', errors='replace'):
            try: event = json.loads(line)
            except json.JSONDecodeError: continue
            if not isinstance(event, dict): continue
            if event.get('type') == 'turn.completed': usage = usage_fields(event)[:4]
            item = event.get('item', {})
            if isinstance(item, dict) and item.get('type') == 'agent_message':
                text = item.get('text') or item.get('content')
                if isinstance(text, str): final = text
    except OSError: pass
else:
    try: final = open(raw_path, encoding='utf-8', errors='replace').read()
    except OSError: pass
    message_samples, chunk_samples, saw_header = [], [], False
    for root, _, files in os.walk(session_root):
        for name in files:
            if not name.endswith('.jsonl'): continue
            try:
                for line in open(os.path.join(root, name), encoding='utf-8', errors='replace'):
                    try: record = json.loads(line)
                    except json.JSONDecodeError: continue
                    event = record.get('event', record) if isinstance(record, dict) else {}
                    if not isinstance(event, dict): continue
                    if event.get('type') == 'request/header':
                        data = event.get('data', {})
                        header = data.get('header', data) if isinstance(data, dict) else {}
                        config = header.get('config', header) if isinstance(header, dict) else {}
                        if not isinstance(config, dict):
                            config_valid = False
                            continue
                        saw_header = True
                        if (config.get('provider') != 'openai-codex' or config.get('model') != 'gpt-5.6-luna' or
                            config.get('reasoningEffort') != 'low' or header.get('reason') == 'fallback'):
                            config_valid = False
                    elif event.get('type') == 'assistant/message':
                        # The pinned DSH projection carries authoritative
                        # provider usage on assistant/message. A message is
                        # emitted once per provider request, so using these
                        # records avoids counting streamed chunks repeatedly.
                        data = event.get('data', {})
                        # Ignore legacy/mirrored usage-only records. A real
                        # projected assistant message includes its message
                        # payload alongside the usage object.
                        if not isinstance(data, dict) or not isinstance(data.get('message'), dict):
                            continue
                        raw = usage_fields(data)
                        if raw[2] is not None and raw[4] is not None and raw[4] >= raw[2]:
                            message_samples.append((raw[4] - raw[2], raw[1], raw[2]))
                    elif event.get('type') == 'assistant/chunk':
                        data = event.get('data', {})
                        chunk = data.get('chunk', {}) if isinstance(data, dict) else {}
                        if isinstance(chunk, dict) and chunk.get('type') == 'usage':
                            raw = usage_fields(chunk)
                            # Pinned pi-ai's totalTokens is authoritative. Input is
                            # total-output, including cached input; never add cache.
                            if raw[2] is not None and raw[4] is not None and raw[4] >= raw[2]:
                                chunk_samples.append((raw[4] - raw[2], raw[1], raw[2]))
            except OSError: config_valid = False
    if not saw_header: config_valid = False
    samples = message_samples or chunk_samples
    if samples:
        cached_known = all(sample[1] is not None for sample in samples)
        usage = (sum(sample[0] for sample in samples),
                 sum(sample[1] for sample in samples) if cached_known else None,
                 sum(sample[2] for sample in samples), None)

def contains_quota_or_rate_limit():
    # Inspect internally only.  Never propagate raw provider diagnostics into
    # public artifacts, where they might contain account or request details.
    for path in (raw_path, stderr_path):
        if not path:
            continue
        try:
            text = open(path, encoding='utf-8', errors='replace').read().lower()
        except OSError:
            continue
        if 'quota' in text or 'rate_limit' in text or 'rate limit' in text:
            return True
    return False

quota_or_rate_limit = contains_quota_or_rate_limit()
status = 'completed' if exit_code == 0 else ('timed_out' if exit_code == 124 else 'failed')
failure_reason = ''
if quota_or_rate_limit:
    # These are failed attempts, not a capability/preflight failure. This
    # deliberately wins over a missing DSH request/header record.
    status, failure_reason = 'failed', 'provider quota or rate limit'
elif kind == 'dsh' and not config_valid:
    status, failure_reason = 'unsupported', 'resolved DSH session configuration is missing or not pinned'
quality = 'provider_reported' if any(x is not None for x in usage[:3]) else 'unavailable'
if not (kind == 'codex' and os.path.exists(final_path)):
    with open(final_path, 'w', encoding='utf-8') as f: f.write(final)
result = {'status': status, 'exit_code': exit_code, 'elapsed_millis': round(elapsed * 1000),
          'usage': {'input_tokens': usage[0], 'cached_input_tokens': usage[1],
                    'output_tokens': usage[2], 'reasoning_output_tokens': usage[3],
                    'token_quality': quality}}
if failure_reason: result['failure_reason'] = failure_reason
with open(result_path, 'w', encoding='utf-8') as f: json.dump(result, f, sort_keys=True); f.write('\n')
