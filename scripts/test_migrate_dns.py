import copy
import unittest

from migrate_dns import migrate_record, plan_records


class FakeCloudflare:
    def __init__(self, record, ignore_write=False):
        self.record = copy.deepcopy(record)
        self.ignore_write = ignore_write
        self.writes = []

    def request(self, method, path, body=None):
        if method == "PATCH":
            self.writes.append(body)
            if not self.ignore_write:
                self.record.update(body)
        return {"result": copy.deepcopy(self.record)}


class MigrationTest(unittest.TestCase):
    def setUp(self):
        self.record = dict(id="1", name="node.example.com", type="A", content="203.0.113.1", ttl=120, proxied=False)

    def test_exact_address_and_name_scope(self):
        other = dict(self.record, id="2", content="203.0.113.10")
        self.assertEqual(plan_records([other, self.record], "203.0.113.1"), [self.record])
        self.assertEqual(plan_records([self.record], "203.0.113.1", ["other.example.com"]), [])

    def test_patch_only_content_and_read_back(self):
        api = FakeCloudflare(self.record)
        result = migrate_record(api, self.record, "203.0.113.2")
        self.assertEqual(api.writes, [{"content": "203.0.113.2"}])
        self.assertEqual(result, dict(self.record, content="203.0.113.2"))

    def test_accepted_but_ineffective_write_is_failure(self):
        with self.assertRaisesRegex(RuntimeError, "read-back"):
            migrate_record(FakeCloudflare(self.record, ignore_write=True), self.record, "203.0.113.2")

    def test_concurrent_edit_is_not_overwritten(self):
        api = FakeCloudflare(dict(self.record, content="203.0.113.99"))
        with self.assertRaisesRegex(RuntimeError, "changed since preview"):
            migrate_record(api, self.record, "203.0.113.2")
        self.assertEqual(api.writes, [])


if __name__ == "__main__":
    unittest.main()
