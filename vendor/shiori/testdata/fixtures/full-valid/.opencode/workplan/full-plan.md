# Full plan

## Goal
Exercise every known field

## Scope
- core
- adapter

## Non-goals
- worktrees

## Constraints
- no automatic migration

## Relevant files
- src/a.ts
- src/b.ts

## Spec files
- docs/spec-a.md
- docs/nested/spec-b.md

## Execution phases
### 1. Phase A <!-- workplan-phase-id: phase-a -->
- Status: completed
- Id: phase-a
#### 1.1 A1 <!-- workplan-step-id: step-a1 -->
- Status: completed
- Id: step-a1
- Target: src/a.ts
- Action: Do A1
- Validation: Check A1
#### 1.2 A2 <!-- workplan-step-id: step-a2 -->
- Status: completed
- Id: step-a2
- Action: Do A2
- Validation: Check A2

### 2. Phase B <!-- workplan-phase-id: phase-b -->
- Status: in_progress
- Id: phase-b
#### 2.1 B1 <!-- workplan-step-id: step-b1 -->
- Status: in_progress
- Id: step-b1
- Action: Do B1
- Validation: Check B1
#### 2.2 B2 <!-- workplan-step-id: step-b2 -->
- Status: draft
- Id: step-b2
- Action: Do B2
- Validation: Check B2

### 3. Phase C <!-- workplan-phase-id: phase-c -->
- Status: draft
- Id: phase-c
#### 3.1 C1 <!-- workplan-step-id: step-c1 -->
- Status: draft
- Id: step-c1
- Action: Do C1
- Validation: Check C1

## Adversarial review findings
- [blocker] Blocker finding(open)— why [source: a.ts:1]
- [critical] Critical finding(resolved)
- [major] Major finding(open)— d
- [minor] Minor finding(open) [source: b.ts:2]
- [note] Note finding(open)
- [question] Question finding(open)

## Notes
- first note
- second note

## Status
- Overall status: in_progress
- Metadata file: .opencode/workplan/full-plan.json
- Detailed plan file: .opencode/workplan/full-plan.md
