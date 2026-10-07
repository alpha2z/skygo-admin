# Runtime plugins

Agent plugin configuration may set `service_actions` to an explicit map from every bound service ID to its nonempty subset of the plugin `actions`. Local service `extensions` must agree for these actions. Omission preserves the legacy all-actions-per-service rule. Unknown services/actions and duplicate grants fail startup; execute, reconcile and observation enforce the same scope.
