import {deploymentChoices} from "../lib/deploymentChoices.js";
import {machinesS, spacesS} from "../state/deployments.js";
import {singleSelect} from "./singleSelect.js";
import {spaceTag} from "./spacesFilter.js";

export function deploymentSelect({deployments, value, onChange, buttonClass, testid = "deployment-select"}) {
    return singleSelect({
        items: () => deploymentChoices(deployments(), spacesS.val, machinesS.val).map((choice) => ({
            id: choice.id,
            label: choice.name,
            detail: choice.sharesName ? choice.nodeNames.join(", ") || `#${choice.id}` : "",
            tail: () => spaceTag(choice.spaceId, choice.spaceName),
            keywords: [choice.spaceName, ...choice.nodeNames].join(" "),
        })),
        value,
        onChange,
        noun: ["deployment", "deployments"],
        ariaLabel: "Deployment",
        buttonClass,
        testid,
    });
}
