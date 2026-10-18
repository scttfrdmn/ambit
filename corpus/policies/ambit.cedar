// ambit policy set. Frozen with ambit.md; the two must say the same thing.
//
// Request shape (built by internal/cedarq):
//   principal  Ambit::Agent    { role }
//   action     Ambit::Action::"<tool>"
//   resource   Ambit::<Type>   { phi, doc_kind, approved_purposes }  (always present)
//   context    { purpose, chain_roles, depth, est_cost, task_spent, task_cap,
//                swarm_spent, swarm_cap }
//              + semantic facts when supplied: sql_touches_identified,
//                payload_contains_identified, doc_on_subject
//
// Default deny. A tool not named below is outside ambit: no permit applies.
// A semantic fact that a rule needs but nobody supplied counts as unsafe.

@id("baseline.known_tools")
permit (
  principal,
  action in [
    Ambit::Action::"query_dataset",
    Ambit::Action::"read_document",
    Ambit::Action::"web_fetch",
    Ambit::Action::"send_message",
    Ambit::Action::"run_compute",
    Ambit::Action::"delegate"
  ],
  resource
);

// ---- Policy 1: PHI access by principal and purpose ----

@id("p1.phi.identified_role")
forbid (
  principal,
  action in [Ambit::Action::"query_dataset", Ambit::Action::"read_document"],
  resource
)
when {
  resource.phi == "identified" &&
  !(["pi", "analyst"].containsAll(context.chain_roles))
};

@id("p1.phi.deidentified_role")
forbid (principal, action == Ambit::Action::"query_dataset", resource)
when {
  resource.phi == "deidentified" &&
  !(["pi", "analyst", "research_assistant"].containsAll(context.chain_roles))
};

@id("p1.phi.purpose")
forbid (principal, action == Ambit::Action::"query_dataset", resource)
when {
  resource.phi != "public" &&
  !resource.approved_purposes.contains(context.purpose)
};

@id("p1.phi.reidentification")
forbid (principal, action == Ambit::Action::"query_dataset", resource)
when {
  resource.phi == "deidentified" &&
  (!(context has sql_touches_identified) || context.sql_touches_identified)
};

@id("p1.phi.document_subject")
forbid (principal, action == Ambit::Action::"read_document", resource)
when {
  resource.phi == "identified" &&
  (!(context has doc_on_subject) || !context.doc_on_subject)
};

// ---- Policy 2: egress ----

@id("p2.egress.no_web_fetch")
forbid (principal, action == Ambit::Action::"web_fetch", resource);

@id("p2.egress.no_identified_outbound")
forbid (principal, action == Ambit::Action::"send_message", resource)
when {
  !(context has payload_contains_identified) || context.payload_contains_identified
};

// ---- Policy 3: cost caps per task and per swarm ----

@id("p3.cost.task_cap")
forbid (principal, action, resource)
when { context.task_spent + context.est_cost > context.task_cap };

@id("p3.cost.swarm_cap")
forbid (principal, action, resource)
when { context.swarm_spent + context.est_cost > context.swarm_cap };

// ---- Policy 4: tool allowlist by role; every principal in the chain ----

@id("p4.allowlist.query_dataset")
forbid (principal, action == Ambit::Action::"query_dataset", resource)
unless { ["pi", "analyst", "research_assistant"].containsAll(context.chain_roles) };

@id("p4.allowlist.read_document")
forbid (principal, action == Ambit::Action::"read_document", resource)
unless {
  ["pi", "analyst", "research_assistant", "external_collaborator"].containsAll(context.chain_roles)
};

@id("p4.allowlist.web_fetch")
forbid (principal, action == Ambit::Action::"web_fetch", resource)
unless { ["pi", "analyst"].containsAll(context.chain_roles) };

@id("p4.allowlist.send_message")
forbid (principal, action == Ambit::Action::"send_message", resource)
unless {
  ["pi", "analyst", "research_assistant", "external_collaborator"].containsAll(context.chain_roles)
};

@id("p4.allowlist.run_compute")
forbid (principal, action == Ambit::Action::"run_compute", resource)
unless { ["pi", "analyst"].containsAll(context.chain_roles) };

@id("p4.allowlist.delegate")
forbid (principal, action == Ambit::Action::"delegate", resource)
unless { ["pi", "analyst"].containsAll(context.chain_roles) };

// ---- Policy 5: delegation depth <= 3 ----

@id("p5.depth.max")
forbid (principal, action, resource)
when { context.depth > 3 };

@id("p5.depth.delegate")
forbid (principal, action == Ambit::Action::"delegate", resource)
when { context.depth + 1 > 3 };
