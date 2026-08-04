import van from "vanjs-core";
import {apiTokenGenerator} from "../components/apiTokenGenerator.js";

const {div} = van.tags;

export function usersPage() {
    return div(
        {class: "app-scroll flex-1 min-h-0 overflow-auto p-3 flex flex-col gap-3"},
        apiTokenGenerator(),
    );
}
